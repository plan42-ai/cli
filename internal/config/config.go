package config

type Runner struct {
	URL            string `toml:"url"`
	RunnerToken    string `toml:"token"`
	SkipSSLVerify  bool   `toml:"skip_ssl_verify,omitempty"`
	Runtime        string `toml:"runtime"`
	OpenAIEndpoint string `toml:"openai_endpoint,omitempty"`
	OpenAIToken    string `toml:"openai_token,omitempty"`
	ClaudeEndpoint string `toml:"claude_endpoint,omitempty"`
	ClaudeToken    string `toml:"claude_token,omitempty"`
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
