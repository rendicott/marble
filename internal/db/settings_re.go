package db

import "regexp"

func reCompile(p string) error {
	_, err := regexp.Compile(p)
	return err
}
