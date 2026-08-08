package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/spf13/cobra"
)

// buildPsqlArgs returns the psql arguments and the environment additions (PGPASSWORD)
// for connecting to the configured database.
func buildPsqlArgs(dbc config.DatabaseConfig) (args []string, env []string) {
	args = []string{"-h", dbc.Host, "-p", strconv.Itoa(dbc.Port), "-U", dbc.User, "-d", dbc.Database}
	env = []string{"PGPASSWORD=" + dbc.Password}
	return args, env
}

// buildRedisArgs returns the redis-cli arguments for the configured Redis (-n only
// when a non-zero DB index is set). The password is NOT passed as an argv (which
// would leak in `ps`); it goes via the REDISCLI_AUTH env — see buildRedisEnv.
func buildRedisArgs(r config.RedisConfig) []string {
	args := []string{"-h", r.Host, "-p", strconv.Itoa(r.Port)}
	if r.DB != 0 {
		args = append(args, "-n", strconv.Itoa(r.DB))
	}
	return args
}

// buildRedisEnv returns the environment additions for redis-cli: REDISCLI_AUTH when
// a password is configured, so it is not exposed on the command line.
func buildRedisEnv(r config.RedisConfig) []string {
	if r.Password == "" {
		return nil
	}
	return []string{"REDISCLI_AUTH=" + r.Password}
}

// dbshellCmd opens an interactive psql session against the configured database,
// mirroring `django manage.py dbshell`.
func dbshellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dbshell",
		Short: "Open an interactive psql shell to the configured database",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			bin, err := exec.LookPath("psql")
			if err != nil {
				return fmt.Errorf("psql not found on PATH: %w", err)
			}
			args, env := buildPsqlArgs(cfg.Database)
			return runInteractive(bin, args, env)
		},
	}
}

// cacheshellCmd opens an interactive redis-cli session against the configured Redis.
func cacheshellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cacheshell",
		Short: "Open an interactive redis-cli shell to the configured Redis",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			bin, err := exec.LookPath("redis-cli")
			if err != nil {
				return fmt.Errorf("redis-cli not found on PATH: %w", err)
			}
			return runInteractive(bin, buildRedisArgs(cfg.Redis), buildRedisEnv(cfg.Redis))
		},
	}
}

// runInteractive launches bin with args, wiring the child to the current terminal
// so it behaves as an interactive shell, and appends env to the process environment.
func runInteractive(bin string, args, env []string) error {
	c := exec.Command(bin, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), env...)
	return c.Run()
}
