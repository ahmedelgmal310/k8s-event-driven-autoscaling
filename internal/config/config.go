// Package config reads typed settings from environment-style lookups.
package config

import (
	"fmt"
	"strconv"
	"time"
)

// Getenv looks up a variable, like os.Getenv.
type Getenv func(string) string

// String returns the value of key, or def when it is unset or empty.
func String(getenv Getenv, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// Int returns the integer value of key, or def when it is unset.
func Int(getenv Getenv, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

// Duration returns the duration value of key (for example "250ms"), or def when it is unset.
func Duration(getenv Getenv, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", key, v)
	}
	return d, nil
}
