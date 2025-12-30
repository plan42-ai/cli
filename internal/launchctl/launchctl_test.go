package launchctl_test

import (
	"testing"
	"time"

	"github.com/plan42-ai/cli/internal/launchctl"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/stretchr/testify/require"
)

func TestBuildLaunchAgentPlist(t *testing.T) {
	agent := launchctl.Agent{
		Name: "ai.plan42.runner",
		Argv: []string{
			"/opt/homebrew/bin/plan42-runner",
			"--config-file",
			"/Users/example/config/plan42-runner.toml",
		},
		ExitTimeout: util.Pointer(5 * time.Minute),
	}

	actual, err := agent.ToXML()
	require.NoError(t, err)

	const expected = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>ai.plan42.runner</string>
    <key>ProgramArguments</key>
    <array>
      <string>/opt/homebrew/bin/plan42-runner</string>
      <string>--config-file</string>
      <string>/Users/example/config/plan42-runner.toml</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ExitTimeOut</key>
    <integer>300</integer>
  </dict>
</plist>
`

	require.Equal(t, expected, actual)
}
