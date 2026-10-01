package initflow

// Step is one stage of `edgectl init`.
type Step func(*Context) error
