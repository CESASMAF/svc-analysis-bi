package configs

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMissingSalt        = errors.New("config: PATIENT_HASH_SALT is required and must not be empty")
	ErrMissingJWKS        = errors.New("config: JWKS_URL is required when AUTH_REQUIRED=true")
	ErrInvalidPort        = errors.New("config: PORT must be between 1 and 65535")
	ErrInvalidDBConfig    = errors.New("config: DB_USER and DB_PASSWORD are required")
	ErrInvalidMaxConns    = errors.New("config: DB_MAX_CONNS must be a positive integer")
	ErrInvalidIdleTimeout = errors.New("config: DB_IDLE_TIMEOUT must be a valid Go duration string")
)

type ServerConfig struct {
	Port int
	Host string
}

type DatabaseConfig struct {
	Host        string
	Port        int
	User        string
	Password    string
	Name        string
	MaxConns    int
	IdleTimeout time.Duration
}

type NATSConfig struct {
	URL      string
	Stream   string
	Consumer string
}

type AuthConfig struct {
	JWKSUrl          string
	AuthRequired     bool
	ExpectedIssuer   string
	ExpectedAudience string
}

type Config struct {
	Server          ServerConfig
	Database        DatabaseConfig
	NATS            NATSConfig
	Auth            AuthConfig
	PatientHashSalt string
	LogLevel        string
}

// Load reads configuration from environment variables with defaults.
func Load() (Config, error) {
	salt := secretEnv("PATIENT_HASH_SALT")
	if salt == "" {
		return Config{}, ErrMissingSalt
	}

	port, err := envInt("PORT", 8080)
	if err != nil {
		return Config{}, ErrInvalidPort
	}
	if port < 1 || port > 65535 {
		return Config{}, ErrInvalidPort
	}

	dbPort, err := envInt("DB_PORT", 5432)
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid DB_PORT: %w", err)
	}
	if dbPort < 1 || dbPort > 65535 {
		return Config{}, ErrInvalidDBConfig
	}

	maxConns, err := envInt("DB_MAX_CONNS", 10)
	if err != nil {
		return Config{}, ErrInvalidMaxConns
	}
	if maxConns < 1 {
		return Config{}, ErrInvalidMaxConns
	}

	idleTimeout, err := envDuration("DB_IDLE_TIMEOUT", 5*time.Minute)
	if err != nil {
		return Config{}, ErrInvalidIdleTimeout
	}

	cfg := Config{
		Server: ServerConfig{
			Port: port,
			Host: envStr("SERVER_HOST", "0.0.0.0"),
		},
		Database: DatabaseConfig{
			Host:        envStr("DB_HOST", "localhost"),
			Port:        dbPort,
			User:        os.Getenv("DB_USER"),
			Password:    secretEnv("DB_PASSWORD"),
			Name:        envStr("DB_NAME", "analysis_bi"),
			MaxConns:    maxConns,
			IdleTimeout: idleTimeout,
		},
		NATS: NATSConfig{
			URL:      envStr("NATS_URL", "nats://localhost:4222"),
			Stream:   envStr("NATS_STREAM", "SOCIAL_CARE_EVENTS"),
			Consumer: envStr("NATS_CONSUMER", "analysis-bi"),
		},
		Auth: AuthConfig{
			JWKSUrl:          os.Getenv("JWKS_URL"),
			AuthRequired:     envBool("AUTH_REQUIRED", true),
			ExpectedIssuer:   os.Getenv("AUTH_ISSUER"),
			ExpectedAudience: os.Getenv("AUTH_AUDIENCE"),
		},
		PatientHashSalt: salt,
		LogLevel:        envStr("LOG_LEVEL", "info"),
	}

	return cfg, nil
}

// Validate checks the Config for internal consistency.
func (c Config) Validate() error {
	if c.PatientHashSalt == "" {
		return ErrMissingSalt
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return ErrInvalidPort
	}
	if c.Database.User == "" || c.Database.Password == "" {
		return ErrInvalidDBConfig
	}
	if c.Database.MaxConns < 1 {
		return ErrInvalidMaxConns
	}
	if c.Auth.AuthRequired && c.Auth.JWKSUrl == "" {
		return ErrMissingJWKS
	}
	return nil
}

// DSN returns a PostgreSQL connection string.
func (c DatabaseConfig) DSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.Name,
	}
	return u.String()
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.Atoi(v)
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return time.ParseDuration(v)
}

// secretEnv lê um secret preferindo o arquivo apontado por `<KEY>_FILE`
// (Docker secrets / OpenBao montam em /run/secrets), com fallback no env `<KEY>`.
//
// POR QUÊ: secrets (senha do banco, salt de anonimização) não devem vir de variável
// de ambiente — env aparece em `docker inspect`, em /proc/<pid>/environ e pode vazar
// em log. Padrão das imagens oficiais (sufixo `_FILE`). A imagem deste serviço é
// `scratch` (sem shell) → não há entrypoint-wrapper possível; o app lê o arquivo
// aqui. Mantém o env como FALLBACK (dev/compat).
func secretEnv(key string) string {
	if path := os.Getenv(key + "_FILE"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return os.Getenv(key)
}
