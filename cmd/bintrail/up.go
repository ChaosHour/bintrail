package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"

	"github.com/dbtrail/bintrail/internal/cliutil"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "One command: preflight + init + stream (the friction-free quickstart)",
	Long: `Runs preflight checks (equivalent to 'bintrail doctor'), creates the
index tables if they do not exist (equivalent to 'bintrail init'), and starts
the replication stream (equivalent to 'bintrail stream'). Re-running 'bintrail
up' is idempotent: it skips work that's already done and resumes the stream
from its saved checkpoint.

This is the friction-free entry point for new bintrail installations. The
underlying 'init', 'snapshot', 'index', and 'stream' commands remain available
for advanced workflows (e.g. running them on separate machines or for
debugging).

If --server-id is not provided, a deterministic ID is derived from
host:user:dbname of the source DSN, mapped into a high range to reduce
collision odds with existing replicas.

Examples:

  bintrail up --source-dsn "$SRC" --index-dsn "$IDX"
  bintrail up --source-dsn "$SRC" --index-dsn "$IDX" --skip-doctor
  bintrail up --source-dsn "$SRC" --index-dsn "$IDX" --schemas mydb,otherdb`,
	RunE: runUp,
}

var (
	upSourceDSN   string
	upIndexDSN    string
	upServerID    uint32
	upSchemas     string
	upTables      string
	upBatchSize   int
	upCheckpoint  int
	upMetricsAddr string
	upPartitions  int
	upSkipDoctor  bool
	upFormat      string
)

func init() {
	upCmd.Flags().StringVar(&upSourceDSN, "source-dsn", "", "DSN for the source MySQL server (required)")
	upCmd.Flags().StringVar(&upIndexDSN, "index-dsn", "", "DSN for the index MySQL database (required)")
	upCmd.Flags().Uint32Var(&upServerID, "server-id", 0, "MySQL replica server ID (default: hash of source host:user:dbname)")
	upCmd.Flags().StringVar(&upSchemas, "schemas", "", "Comma-separated schemas to index (default: all user schemas)")
	upCmd.Flags().StringVar(&upTables, "tables", "", "Comma-separated tables to index (default: all)")
	upCmd.Flags().IntVar(&upBatchSize, "batch-size", 1000, "Events per batch INSERT")
	upCmd.Flags().IntVar(&upCheckpoint, "checkpoint", 10, "Checkpoint interval in seconds")
	upCmd.Flags().StringVar(&upMetricsAddr, "metrics-addr", "", "Address to expose Prometheus metrics (e.g. :9090); empty = disabled")
	upCmd.Flags().IntVar(&upPartitions, "partitions", 48, "Hourly partitions to create on first init")
	upCmd.Flags().BoolVar(&upSkipDoctor, "skip-doctor", false, "Skip the preflight checks (useful when you've already verified with `bintrail doctor`)")
	upCmd.Flags().StringVar(&upFormat, "format", "text", "Output format: text or json")
	_ = upCmd.MarkFlagRequired("source-dsn")
	_ = upCmd.MarkFlagRequired("index-dsn")
	bindCommandEnv(upCmd)
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) error {
	if !cliutil.IsValidOutputFormat(upFormat) {
		return fmt.Errorf("invalid --format %q; must be text or json", upFormat)
	}

	// ── Phase 1: Preflight ──────────────────────────────────────────────────
	if !upSkipDoctor {
		fmt.Fprintln(os.Stderr, "=== Phase 1/3: Preflight checks ===")
		if err := runDoctorTo(cmd.Context(), os.Stderr, "text", upSourceDSN, upIndexDSN, upSchemas); err != nil {
			return fmt.Errorf("preflight failed (use --skip-doctor to bypass at your own risk): %w", err)
		}
		fmt.Fprintln(os.Stderr)
	}

	// ── Phase 2: Init ───────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "=== Phase 2/3: Initializing index database ===")
	if err := runUpInit(cmd); err != nil {
		return fmt.Errorf("init failed: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	// ── Phase 3: Stream ─────────────────────────────────────────────────────
	fmt.Fprintln(os.Stderr, "=== Phase 3/3: Streaming ===")
	return runUpStream(cmd, args)
}

// runUpInit calls runInit with up's flag values. We share the parent context
// so Ctrl-C propagates during the init phase (table creation + optional S3
// bucket provisioning, both of which can block on remote IO).
func runUpInit(cmd *cobra.Command) error {
	initIndexDSN = upIndexDSN
	initPartitions = upPartitions
	initFormat = "text"
	initEncrypt = false
	initS3Bucket = ""
	initS3Region = "us-east-1"
	initS3ARN = ""

	subCmd := &cobra.Command{}
	subCmd.SetContext(cmd.Context())
	return runInit(subCmd, nil)
}

// runUpStream delegates to runStream by populating its flag vars from up's.
// The snapshot step is handled inside runStream via auto-snapshot when no
// snapshot exists and --source-dsn is set.
func runUpStream(cmd *cobra.Command, args []string) error {
	strmIndexDSN = upIndexDSN
	strmSourceDSN = upSourceDSN

	if upServerID == 0 {
		id, err := deriveServerID(upSourceDSN)
		if err != nil {
			return fmt.Errorf("cannot auto-derive --server-id from --source-dsn: %w (pass --server-id explicitly to bypass)", err)
		}
		strmServerID = id
		fmt.Fprintf(os.Stderr, "Auto-derived server-id from source DSN: %d\n", strmServerID)
	} else {
		strmServerID = upServerID
	}

	strmStartFile = ""
	strmStartPos = 4
	strmStartGTID = ""
	strmBatchSize = upBatchSize
	strmSchemas = upSchemas
	strmTables = upTables
	strmCheckpoint = upCheckpoint
	strmMetricsAddr = upMetricsAddr
	strmSSLMode = "preferred"
	strmSSLCA = ""
	strmSSLCert = ""
	strmSSLKey = ""
	strmFormat = upFormat
	strmReset = false
	strmNoGapFill = false
	strmGapTimeout = 30

	return runStream(cmd, args)
}

// deriveServerID returns a deterministic uint32 server-id by hashing the
// source DSN's host:user:dbname triple. The same DSN always produces the same
// ID, so `bintrail up` resumes cleanly across restarts without the user
// remembering what server-id they used last time.
//
// Returns an error when the DSN cannot be parsed — callers must handle this
// rather than silently substituting a non-deterministic value, because a
// per-invocation ID breaks the resume-from-checkpoint contract (MySQL would
// treat each restart as a new replica).
func deriveServerID(dsn string) (uint32, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return 0, fmt.Errorf("parse DSN: %w", err)
	}
	seed := fmt.Sprintf("%s|%s|%s", cfg.Addr, cfg.User, cfg.DBName)
	sum := sha256.Sum256([]byte(seed))
	raw := binary.BigEndian.Uint32(sum[:4])
	// Map into [100000000, 4294967294]: subtract floor from uint32 range, mod
	// into the resulting width, then add the floor back. Keeps the value high
	// enough that collisions with typical hand-picked replica server-ids are
	// unlikely.
	const floor = uint32(100000000)
	const width = uint32(4294967295 - floor) // 4194967295
	return (raw % width) + floor, nil
}
