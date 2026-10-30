package walk

import (
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Layer is a kind of change, in the order a reviewer reads them: the data
// first, then what reads and writes it, the logic on top, the API that
// exposes it, configuration that uses it, the screens, then tests and docs.
type Layer struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Rank int    `json:"rank"`
}

// Layers are the built-in ones.
var Layers = []Layer{
	{"schema", "Database and migrations", 10},
	{"model", "Data model and types", 20},
	{"data", "Data access", 30},
	{"logic", "Logic and services", 40},
	{"api", "API and routes", 50},
	{"config", "Configuration, prompts and integrations", 60},
	{"ui", "User interface", 70},
	{"tests", "Tests", 80},
	{"docs", "Documentation", 90},
	{"build", "Build, dependencies and CI", 95},
	{"noise", "Generated and lock files", 100},
}

// LayerName is a layer's display name ("" for an unknown key).
func (c *RepoConfig) LayerName(key string) string {
	if c != nil {
		for _, l := range c.layers {
			if l.Key == key {
				return l.Name
			}
		}
	}
	for _, l := range Layers {
		if l.Key == key {
			return l.Name
		}
	}
	return ""
}

// Rank is a layer's place in the reading order.
func (c *RepoConfig) Rank(key string) int {
	if c != nil {
		for _, l := range c.layers {
			if l.Key == key {
				return l.Rank
			}
		}
	}
	for _, l := range Layers {
		if l.Key == key {
			return l.Rank + 1000
		}
	}
	return 1500
}

// RepoConfig is a repository's own .pocket/review.yml:
//
//	hints: |
//	  Free text for the AI: how this codebase is put together.
//	order:
//	  - name: Migrations
//	    paths: ["packages/db/migrations/**"]
//	  - name: Repositories
//	    paths: ["**/repositories/**"]
//	noise: ["**/*.snap", "src/generated/**"]
//
// Its layers come first in the reading order; files they do not match fall
// back to the built-in layers.
type RepoConfig struct {
	Hints string `yaml:"hints" json:"hints"`
	Order []struct {
		Name  string   `yaml:"name" json:"name"`
		Paths []string `yaml:"paths" json:"paths"`
	} `yaml:"order" json:"order"`
	Noise []string `yaml:"noise" json:"noise"`

	layers []Layer
	match  [][]*regexp.Regexp
	noise  []*regexp.Regexp
}

// ConfigPath is where a repository keeps its config.
const ConfigPath = ".pocket/review.yml"

// ParseConfig reads a repository config (YAML or JSON).
func ParseConfig(b []byte) (*RepoConfig, error) {
	var c RepoConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	for i, o := range c.Order {
		name := strings.TrimSpace(o.Name)
		if name == "" {
			continue
		}
		var res []*regexp.Regexp
		for _, g := range o.Paths {
			if re := globRe(g); re != nil {
				res = append(res, re)
			}
		}
		c.layers = append(c.layers, Layer{Key: "repo" + itoa(i+1), Name: name, Rank: (i + 1) * 10})
		c.match = append(c.match, res)
	}
	for _, g := range c.Noise {
		if re := globRe(g); re != nil {
			c.noise = append(c.noise, re)
		}
	}
	return &c, nil
}

// globRe turns a glob (**, *, ?) into an anchored regexp.
func globRe(g string) *regexp.Regexp {
	g = strings.TrimPrefix(strings.TrimSpace(g), "/")
	if g == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if strings.HasSuffix(g, "/") {
		b.WriteString(".*")
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

var (
	lockFiles = map[string]bool{"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true, "bun.lock": true,
		"go.sum": true, "cargo.lock": true, "poetry.lock": true, "pipfile.lock": true, "composer.lock": true, "gemfile.lock": true,
		"mix.lock": true, "pubspec.lock": true, "podfile.lock": true, "flake.lock": true, "uv.lock": true, "deno.lock": true, "packages.lock.json": true}
	generatedRe   = regexp.MustCompile(`(?i)(\.pb\.go|_pb2\.py|\.pb\.(cc|h)|\.g\.dart|\.gen\.[a-z]+|\.generated\.[a-z]+|\.min\.(js|css)|\.map|\.snap)$|(^|/)(__generated__|generated|dist|build|vendor|third_party|node_modules)/|(^|/)__snapshots__/`)
	generatedHead = regexp.MustCompile(`(?i)(code generated .* do not edit|@generated|auto-?generated|this file was generated)`)

	schemaRe = regexp.MustCompile(`(?i)(^|/)(migrations?|migrate|alembic|flyway|liquibase|schema-migrations)/|\.sql$|(^|/)schema\.prisma$|(^|/)db/schema\.rb$|(^|/)drizzle/|\.migration\.[a-z]+$|(^|/)changelog.*\.(xml|ya?ml)$`)
	testRe   = regexp.MustCompile(`(?i)(_test\.go|\.(test|spec)\.[a-z]+|_spec\.rb|Tests?\.(cs|java|kt|swift))$|(^|/)(tests?|__tests__|spec|specs|e2e|cypress|playwright|fixtures|testdata)/|(^|/)test_[^/]+\.py$|_test\.py$|conftest\.py$`)
	docsRe   = regexp.MustCompile(`(?i)\.(md|mdx|rst|adoc|txt)$|(^|/)(docs?|documentation)/|(^|/)(license|changelog|readme)[^/]*$`)
	buildRe  = regexp.MustCompile(`(?i)(^|/)\.github/|(^|/)(dockerfile|containerfile|makefile|justfile|taskfile\.ya?ml|docker-compose[^/]*\.ya?ml|compose\.ya?ml|\.dockerignore|\.gitignore|\.gitattributes|\.editorconfig|\.nvmrc|\.tool-versions)$|(^|/)(package\.json|go\.mod|cargo\.toml|pyproject\.toml|requirements[^/]*\.txt|setup\.(py|cfg)|gemfile|build\.gradle(\.kts)?|pom\.xml|[^/]*\.csproj|tsconfig[^/]*\.json|vite\.config\.[a-z]+|webpack\.config\.[a-z]+|rollup\.config\.[a-z]+|eslint[^/]*|\.eslintrc[^/]*|\.prettierrc[^/]*|babel\.config\.[a-z]+|turbo\.json|nx\.json|lerna\.json|\.gitlab-ci\.yml|jenkinsfile|\.circleci/.*|flake\.nix|default\.nix)$|(^|/)(scripts|ci|\.husky|deploy|infra|terraform|helm|k8s|charts)/|\.(tf|tfvars)$`)
	modelRe  = regexp.MustCompile(`(?i)(^|/)(models?|entities|entity|domain/model|types?|typings|schemas?|dtos?|interfaces|contracts|proto|protos)/|\.(proto|graphql|gql|avsc)$|(^|/)[^/]*(model|entity|types|dto|schema)s?\.[a-z]+$|\.d\.ts$`)
	dataRe   = regexp.MustCompile(`(?i)(^|/)(repositor(y|ies)|repos?|dao|daos|store|stores|storage|persistence|db|database|queries|query|orm|prisma|data|datasources?|mappers?)/|(^|/)[^/]*(repository|repo|dao|store|queries|query)\.[a-z]+$`)
	apiRe    = regexp.MustCompile(`(?i)(^|/)(routes?|routers?|controllers?|handlers?|api|apis|endpoints?|resolvers?|graphql|rest|rpc|trpc|server|views/api|http|web/api)/|(^|/)[^/]*(route|router|routes|controller|handler|handlers|resolver|endpoint|api)s?\.[a-z]+$|(^|/)app/api/|openapi[^/]*\.(ya?ml|json)$|swagger[^/]*\.(ya?ml|json)$`)
	uiRe     = regexp.MustCompile(`(?i)\.(tsx|jsx|vue|svelte|astro|css|scss|sass|less|styl|html?|hbs|ejs|erb|haml|twig|blade\.php|xib|storyboard|swiftui)$|(^|/)(components?|pages?|views?|screens?|layouts?|ui|frontend|web|client|app/\(|public|static|assets|styles|templates|widgets)/`)
	configRe = regexp.MustCompile(`(?i)\.(ya?ml|toml|ini|conf|cfg|env(\.[a-z]+)?|properties|plist)$|(^|/)(config|configs|configuration|settings|prompts?|agents?|chatbot|bots?|llm|ai|tools|workflows|flows|i18n|locales?|translations?)/|(^|/)\.env[^/]*$|\.json$`)
	logicRe  = regexp.MustCompile(`(?i)(^|/)(services?|domain|usecases?|use-cases|core|lib|libs|internal|pkg|src|app|modules?|features?|jobs?|workers?|tasks?|parsers?|utils?|helpers?|common|shared|business|engine|processors?|pipelines?|handlers/events)/|\.(go|py|rb|java|kt|scala|rs|cs|php|ex|exs|clj|swift|m|mm|c|cc|cpp|h|hpp|ts|js|mjs|cjs|dart|lua|r|jl|sh)$`)
)

// Classify puts a changed file in a layer.
func (c *RepoConfig) Classify(p, patch string) string {
	if c != nil {
		for _, re := range c.noise {
			if re.MatchString(p) {
				return "noise"
			}
		}
		for i, res := range c.match {
			for _, re := range res {
				if re.MatchString(p) {
					return c.layers[i].Key
				}
			}
		}
	}
	return classify(p, patch)
}

func classify(p, patch string) string {
	base := strings.ToLower(path.Base(p))
	switch {
	case lockFiles[base], generatedRe.MatchString(p):
		return "noise"
	case looksGenerated(patch):
		return "noise"
	case schemaRe.MatchString(p):
		return "schema"
	case testRe.MatchString(p):
		return "tests"
	case docsRe.MatchString(p):
		return "docs"
	case buildRe.MatchString(p):
		return "build"
	case apiRe.MatchString(p) && !uiRe.MatchString(p):
		return "api"
	case dataRe.MatchString(p) && !uiRe.MatchString(p):
		return "data"
	case modelRe.MatchString(p) && !uiRe.MatchString(p):
		return "model"
	case uiRe.MatchString(p):
		return "ui"
	case configRe.MatchString(p):
		return "config"
	case logicRe.MatchString(p):
		return "logic"
	}
	return "logic"
}

// looksGenerated reads the first added lines for a "generated, do not edit" mark.
func looksGenerated(patch string) bool {
	n := 0
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "+") {
			if generatedHead.MatchString(l) {
				return true
			}
			if n++; n > 6 {
				break
			}
		}
	}
	return false
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
