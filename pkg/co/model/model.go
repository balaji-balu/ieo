package model

type COConfig struct {
    Server struct {
        Version  string `koanf:"version"`
        Port string `koanf:"port"`
		Name string `koanf:"name"`
		Metricsport string `koanf:"metricsport"`
    } `koanf:"server"`

    Appregistry struct {
        Repo      string `koanf:"repo"`
        Branch string `koanf:"branch"`
    } `koanf:"appregistry"`

    Git struct {
        Owner     string `koanf:"owner"`
		Repo string `koanf:"repo"`
		Branch string `koanf:"branch"`
		Private bool `koanf:"private"`
		TokenEnv string `koanf:"token_env"`
    } `koanf:"git"`
    Mode string `koanf:"mode"`    
}

