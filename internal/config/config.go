package config

type Runner struct {
	URL           string `toml:"url"`
	RunnerToken   string `toml:"token"`
	SkipSSLVerify bool   `toml:"skip_ssl_verify,omitempty"`
	Runtime       string `toml:"runtime"`
}

type GithubInfo struct {
	Name         string `toml:"name"`
	URL          string `toml:"url"`
	ConnectionID string `toml:"connection_id"`
	Token        string `toml:"token"`
}

type Config struct {
	Runner Runner                 `toml:"runner"`
	Github map[string]*GithubInfo `toml:"github"`
}
