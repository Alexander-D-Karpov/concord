package main

import (
	"reflect"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
)

func TestBuildPsqlArgs(t *testing.T) {
	db := config.DatabaseConfig{Host: "db.example", Port: 5433, User: "concord", Password: "s3cret", Database: "concord"}
	args, env := buildPsqlArgs(db)
	wantArgs := []string{"-h", "db.example", "-p", "5433", "-U", "concord", "-d", "concord"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
	if len(env) != 1 || env[0] != "PGPASSWORD=s3cret" {
		t.Errorf("env = %v, want [PGPASSWORD=s3cret]", env)
	}
}

func TestBuildRedisArgs(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.RedisConfig
		want []string
	}{
		{"minimal", config.RedisConfig{Host: "localhost", Port: 6379}, []string{"-h", "localhost", "-p", "6379"}},
		{"with db, password not in argv", config.RedisConfig{Host: "r", Port: 6380, Password: "pw", DB: 3},
			[]string{"-h", "r", "-p", "6380", "-n", "3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRedisArgs(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildRedisArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildRedisEnv verifies the password is carried via REDISCLI_AUTH, not argv.
func TestBuildRedisEnv(t *testing.T) {
	if env := buildRedisEnv(config.RedisConfig{Host: "r", Port: 6379}); env != nil {
		t.Errorf("expected no env without a password, got %v", env)
	}
	env := buildRedisEnv(config.RedisConfig{Host: "r", Port: 6379, Password: "pw"})
	if len(env) != 1 || env[0] != "REDISCLI_AUTH=pw" {
		t.Errorf("expected [REDISCLI_AUTH=pw], got %v", env)
	}
}
