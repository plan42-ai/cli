package poller

type PlatformFields struct {
	ContainerPath string
}

type InvokePlatformFields struct {
	ContainerPath string
}

func WithContainerPath(path string) Option {
	return func(p *Poller) {
		p.ContainerPath = path
	}
}
