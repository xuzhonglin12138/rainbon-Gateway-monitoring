package service

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
)

type RouteGroupRule struct {
	Prefix string
	Group  string
}

type RouteGroupConfig struct {
	UserRules         []RouteGroupRule
	TemplateRules     []RouteGroupRule
	MaxGroupsPerScope int
}

type RouteGroupInput struct {
	AppID       string
	ComponentID string
	URI         string
}

type RouteGroupResolver struct {
	userRules         []RouteGroupRule
	templateRules     []RouteGroupRule
	maxGroupsPerScope int

	mu     sync.Mutex
	groups map[string]map[string]struct{}
}

func NewRouteGroupResolver(cfg RouteGroupConfig) *RouteGroupResolver {
	if cfg.MaxGroupsPerScope <= 0 {
		cfg.MaxGroupsPerScope = 100
	}
	return &RouteGroupResolver{
		userRules:         cfg.UserRules,
		templateRules:     cfg.TemplateRules,
		maxGroupsPerScope: cfg.MaxGroupsPerScope,
		groups:            make(map[string]map[string]struct{}),
	}
}

func (r *RouteGroupResolver) Resolve(input RouteGroupInput) string {
	return r.resolve(input, r.userRules)
}

func (r *RouteGroupResolver) ResolveWithUserRules(input RouteGroupInput, userRules []RouteGroupRule) string {
	return r.resolve(input, userRules)
}

func (r *RouteGroupResolver) resolve(input RouteGroupInput, userRules []RouteGroupRule) string {
	path := normalizePath(input.URI)
	if path == "" || path == "/" {
		return "/other"
	}

	group := matchRules(path, userRules)
	if group == "" {
		group = matchRules(path, r.templateRules)
	}
	if group == "" {
		group = autoNormalize(path)
	}
	if group == "" {
		group = "/other"
	}

	if !r.allowGroup(input, group) {
		return "/other"
	}
	return group
}

func (r *RouteGroupResolver) allowGroup(input RouteGroupInput, group string) bool {
	scope := input.AppID
	if scope == "" {
		scope = input.ComponentID
	}
	if scope == "" {
		scope = "unknown"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.groups[scope]; !ok {
		r.groups[scope] = make(map[string]struct{})
	}
	if _, ok := r.groups[scope][group]; ok {
		return true
	}
	if len(r.groups[scope]) >= r.maxGroupsPerScope {
		return false
	}
	r.groups[scope][group] = struct{}{}
	return true
}

func matchRules(path string, rules []RouteGroupRule) string {
	for _, rule := range rules {
		if rule.Prefix != "" && strings.HasPrefix(path, rule.Prefix) {
			return rule.Group
		}
	}
	return ""
}

func normalizePath(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Path != "" {
		raw = parsed.Path
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	return strings.TrimRight(raw, "/")
}

var (
	numericSegment = regexp.MustCompile(`^\d+$`)
	uuidSegment    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dateSegment    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	hashSegment    = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
)

func autoNormalize(path string) string {
	parts := strings.Split(path, "/")
	changed := false
	for i, part := range parts {
		switch {
		case part == "":
			continue
		case numericSegment.MatchString(part):
			parts[i] = "{id}"
			changed = true
		case uuidSegment.MatchString(part):
			parts[i] = "{uuid}"
			changed = true
		case dateSegment.MatchString(part):
			parts[i] = "{date}"
			changed = true
		case hashSegment.MatchString(part):
			parts[i] = "{hash}"
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(parts, "/")
}
