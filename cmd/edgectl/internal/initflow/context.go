package initflow

// Context carries what the init steps discover and decide.
type Context struct {
	OS         string
	Arch       string
	HasSystemd bool

	COEndpoint string
	SiteID     string
}

// NewContext returns an empty init Context.
func NewContext() *Context {
	return &Context{}
}
