package config

import (
	"github.com/go-viper/mapstructure/v2"
)

// newDecoderConfig builds a mapstructure decoder config that honours
// time.Duration strings and surfaces type-mismatch errors clearly.
func newDecoderConfig(out any) *mapstructure.DecoderConfig {
	return &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
		Metadata:         nil,
		Result:           out,
		WeaklyTypedInput: true,
		TagName:          "koanf",
	}
}
