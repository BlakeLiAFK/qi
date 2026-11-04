package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopi/internal/skills"
)

type skillRootFlags []string

func (s *skillRootFlags) String() string { return strings.Join(*s, ", ") }
func (s *skillRootFlags) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("skill root must not be empty")
	}
	*s = append(*s, value)
	return nil
}
func discoverSkills(ctx context.Context, roots []string) (*skills.Catalog, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	config := skills.Config{}
	for i, path := range roots {
		config.Roots = append(config.Roots, skills.Root{Source: fmt.Sprintf("explicit-%d", i+1), Path: path})
	}
	return skills.Discover(ctx, config)
}
func selectedSkillNames(raw string) []string {
	var names []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}
