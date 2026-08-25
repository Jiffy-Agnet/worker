package config

import (
	"reflect"
	"testing"

	"github.com/Jiffy-Agnet/worker/internal/sandboxdetect"
)

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"A", []string{"A"}},
		{"A,B,C", []string{"A", "B", "C"}},
		{" A , B ,, C ", []string{"A", "B", "C"}},
	}
	for _, c := range cases {
		got := splitAndTrim(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitAndTrim(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestIsLocalAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:6379", true},
		{"localhost:6379", true},
		{"[::1]:6379", true},
		{"redis.example.com:6379", false},
		{"10.0.0.5:6379", false},
	}
	for _, c := range cases {
		if got := isLocalAddr(c.addr); got != c.want {
			t.Errorf("isLocalAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestSecurityWarningsForRemoteInsecureRedis(t *testing.T) {
	cfg := &Config{RedisAddr: "redis.example.com:6379", RedisTLS: false, RedisPassword: ""}
	warnings := cfg.SecurityWarnings()
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings for a remote Redis with no TLS/password, got %d: %v", len(warnings), warnings)
	}
}

func TestSecurityWarningsNoneForLocalRedis(t *testing.T) {
	cfg := &Config{RedisAddr: "127.0.0.1:6379", RedisTLS: false, RedisPassword: ""}
	if warnings := cfg.SecurityWarnings(); len(warnings) != 0 {
		t.Errorf("expected no warnings for a local Redis, got %v", warnings)
	}
}

func TestSecurityWarningsNoneForRemoteSecureRedis(t *testing.T) {
	cfg := &Config{RedisAddr: "redis.example.com:6379", RedisTLS: true, RedisPassword: "secret"}
	if warnings := cfg.SecurityWarnings(); len(warnings) != 0 {
		t.Errorf("expected no warnings when TLS+password are both set, got %v", warnings)
	}
}

func TestParseDetectionRules(t *testing.T) {
	got := parseDetectionRules("Gemfile=ruby, Cargo.toml=rust ,*.csproj=dotnet")
	want := []sandboxdetect.Rule{
		{Pattern: "Gemfile", Key: "ruby"},
		{Pattern: "Cargo.toml", Key: "rust"},
		{Pattern: "*.csproj", Key: "dotnet"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDetectionRules = %+v, want %+v", got, want)
	}
}

func TestParseDetectionRulesEmpty(t *testing.T) {
	if got := parseDetectionRules(""); got != nil {
		t.Errorf("parseDetectionRules(\"\") = %+v, want nil", got)
	}
}

func TestParseImageMap(t *testing.T) {
	got := parseImageMap("ruby=jiffy-sandbox-ruby:1.0.0,rust=jiffy-sandbox-rust:1.0.0")
	want := map[string]string{
		"ruby": "jiffy-sandbox-ruby:1.0.0",
		"rust": "jiffy-sandbox-rust:1.0.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseImageMap = %+v, want %+v", got, want)
	}
}
