package config

type APIConfig struct {
	ListenAddress string `split_words:"true" default:"0.0.0.0:80"`
}
