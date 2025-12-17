package docker

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	validDNSRegex            = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)
	validRepositoryNameRegex = regexp.MustCompile(`^([a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	validPortRegex           = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	validTagRegex            = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)
)

type ImageURI struct {
	Registry     *string
	RegistryPort *string
	Repository   string
	Tag          *string
}

func (i *ImageURI) MarshalText() (text []byte, err error) {
	buf := bytes.NewBuffer(nil)
	if i.Registry != nil {
		buf.WriteString(*i.Registry)
		if i.RegistryPort != nil {
			buf.WriteByte(':')
			buf.WriteString(*i.RegistryPort)
		}
		buf.WriteByte('/')
	}
	buf.WriteString(i.Repository)
	if i.Tag != nil {
		buf.WriteByte(':')
		buf.WriteString(*i.Tag)
	}
	return buf.Bytes(), nil
}

func (i *ImageURI) UnmarshalText(text []byte) error {
	tmp, err := ParseImageURI(string(text))
	if err != nil {
		return err
	}
	*i = *tmp
	return nil
}

func (i *ImageURI) String() string {
	ret, err := i.MarshalText()
	if err != nil {
		panic(err)
	}
	return string(ret)
}

func (i *ImageURI) WithRegistry(registry *string) *ImageURI {
	ret := *i
	ret.Registry = registry
	return &ret
}

func (i *ImageURI) WithDefaultRegistry(registry *string) *ImageURI {
	if i != nil && i.Registry == nil && registry != nil {
		return i.WithRegistry(registry)
	}
	return i
}

func ParseImageURI(uri string) (*ImageURI, error) {
	var ret ImageURI
	// Split the uri by /
	components := strings.Split(uri, "/")
	// If the first component contains a . or : then it is a registry name
	if len(components) > 1 && (strings.Contains(components[0], ".") || strings.Contains(components[0], ":")) {
		parseURIWithRegistry(components, &ret)
	} else {
		parseURIWithoutRegistry(components, &ret)
	}

	// validate tha all the fields have valid values
	if ret.Registry != nil && !validateDNSName(*ret.Registry) {
		return nil, fmt.Errorf("invalid registry: '%v'", *ret.Registry)
	}

	if !validRepositoryName(ret.Repository) {
		return nil, fmt.Errorf("invalid repository: '%v'", ret.Repository)
	}

	if ret.RegistryPort != nil && !validPort(*ret.RegistryPort) {
		return nil, fmt.Errorf("invalid port: '%v'", *ret.RegistryPort)
	}

	if ret.Tag != nil && !validTag(*ret.Tag) {
		return nil, fmt.Errorf("invalid tag: '%v'", *ret.Tag)
	}

	return &ret, nil
}

func parseURIWithoutRegistry(components []string, ret *ImageURI) {
	// Split the last component on ":" to get a tag, if any.
	tagComponents := strings.SplitN(components[len(components)-1], ":", 2)

	if len(tagComponents) == 2 {
		// There is a tag, so process it
		ret.Repository = combineRepo(components[:len(components)-1], tagComponents[0])
		ret.Tag = &tagComponents[1]
	} else {
		// There is no tag, so just process the repository
		ret.Repository = strings.Join(components, "/")
	}
}

func parseURIWithRegistry(components []string, ret *ImageURI) {
	// Split the first component by : to get the port
	portComponents := strings.SplitN(components[0], ":", 2)
	ret.Registry = &portComponents[0]
	if len(portComponents) == 2 {
		ret.RegistryPort = &portComponents[1]
	}

	// Split the last component on ":" to get a tag, if any.
	tagComponents := strings.SplitN(components[len(components)-1], ":", 2)

	if len(tagComponents) == 2 {
		// There is a tag, so process it.
		ret.Repository = combineRepo(components[1:len(components)-1], tagComponents[0])
		ret.Tag = &tagComponents[1]
	} else {
		// There is no tag, so just process the repository.
		ret.Repository = strings.Join(components[1:], "/")
	}
}

func combineRepo(elemes []string, lastElem string) string {
	ret := strings.Join(elemes, "/")
	if ret != "" {
		return fmt.Sprintf("%s/%s", ret, lastElem)
	}
	return lastElem
}

func validTag(s string) bool {
	return validTagRegex.MatchString(s)
}
func validPort(s string) bool {
	if !validPortRegex.MatchString(s) {
		return false
	}
	port, _ := strconv.Atoi(s)
	return port > 0 && port <= 65535
}

func validRepositoryName(repository string) bool {
	return len(repository) <= 255 && validRepositoryNameRegex.MatchString(repository)
}

func validateDNSName(s string) bool {
	return validDNSRegex.MatchString(s)
}
