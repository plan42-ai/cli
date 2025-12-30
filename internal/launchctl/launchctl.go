package launchctl

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/plan42-ai/xml"
)

type Agent struct {
	Name        string
	Argv        []string
	ExitTimeout *time.Duration
	CreateLog   bool
}

type plistDocument struct {
	XMLName xml.Name  `xml:"plist"`
	Version string    `xml:"version,attr"`
	Dict    plistDict `xml:"dict"`
}

type plistDict struct {
	Entries []any `xml:",any"`
}

type keyElement struct {
	XMLName xml.Name `xml:"key"`
	Value   string   `xml:",chardata"`
}

type stringElement struct {
	XMLName xml.Name `xml:"string"`
	Value   string   `xml:",chardata"`
}

type arrayElement struct {
	XMLName xml.Name `xml:"array"`
	Values  []string `xml:"string"`
}

type trueElement struct {
}

func (t trueElement) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	_ = start
	return e.EncodeToken(xml.EmptyElement{
		Name: xml.Name{
			Local: "true",
		},
	})
}

type falseElement struct {
}

func (f falseElement) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	_ = start
	return e.EncodeToken(xml.EmptyElement{
		Name: xml.Name{
			Local: "false",
		},
	})
}

func boolElement(value bool) any {
	if value {
		return trueElement{}
	}
	return falseElement{}
}

type intElement struct {
	XMLName xml.Name `xml:"integer"`
	Value   int      `xml:",chardata"`
}

func (a *Agent) ToXML() (string, error) {
	doc := plistDocument{
		Version: "1.0",
		Dict: plistDict{
			Entries: []any{
				keyElement{Value: "Label"},
				stringElement{Value: a.Name},
				keyElement{Value: "ProgramArguments"},
				arrayElement{Values: a.Argv},
				keyElement{Value: "RunAtLoad"},
				boolElement(true),
				keyElement{Value: "KeepAlive"},
				boolElement(true),
			},
		},
	}

	if a.ExitTimeout != nil {
		doc.Dict.Entries = append(
			doc.Dict.Entries,
			keyElement{Value: "ExitTimeOut"},
			intElement{Value: int(a.ExitTimeout.Seconds())},
		)
	}

	if a.CreateLog {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("unable to determine user home dir: %w", err)
		}
		doc.Dict.Entries = append(
			doc.Dict.Entries,
			keyElement{Value: "StandardErrorPath"},
			stringElement{Value: path.Join(homeDir, "Library", "Logs", a.Name, "log.txt")},
		)
	}

	var builder strings.Builder
	builder.WriteString(xml.Header)
	builder.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple Computer//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	encoder := xml.NewEncoder(&builder)
	encoder.Indent("", "  ")
	err := encoder.Encode(doc)
	if err != nil {
		return "", err
	}
	builder.WriteByte('\n')

	return builder.String(), nil
}

func (a *Agent) Create() error {
	plistPath, err := a.PlistPath()
	if err != nil {
		return err
	}

	plistContent, err := a.ToXML()
	if err != nil {
		return fmt.Errorf("failed to build launchctl agent configuration: %w", err)
	}

	// #nosec G306: It's ok that this is 0644 and not 0600
	err = os.WriteFile(plistPath, []byte(plistContent), 0o644)
	if err != nil {
		return fmt.Errorf("failed to write launchctl agent configuration: %w", err)
	}
	return nil
}

func (a *Agent) PlistPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user home directory: %w", err)
	}

	launchAgentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
	err = os.MkdirAll(launchAgentsDir, 0o755)
	if err != nil {
		return "", fmt.Errorf("failed to create launch agents directory: %w", err)
	}

	plistPath := filepath.Join(launchAgentsDir, fmt.Sprintf("%s.plist", a.Name))
	return plistPath, nil
}

func (a *Agent) FullLabel() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), a.Name)
}
func (a *Agent) Shutdown() error {
	label := fmt.Sprintf("gui/%d", os.Getuid())
	plistPath, err := a.PlistPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("launchctl", "bootout", label, plistPath)
	return cmd.Run()
}

func (a *Agent) Bootstrap() error {
	label := fmt.Sprintf("gui/%d", os.Getuid())
	plistPath, err := a.PlistPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("launchctl", "bootstrap", label, plistPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (a *Agent) Kickstart() error {
	// #nosec: G204 - Subprocess launched with a potential tainted input or cmd arguments
	//    This is ok. The "tainted" arg is gui/uid, where we get the UID from the OS via a system call.
	cmd := exec.Command("launchctl", "kickstart", "-kp", a.FullLabel())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
