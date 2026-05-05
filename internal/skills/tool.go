package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReadSkill is the tool the model calls to load a skill's full body when it
// decides one is relevant. The body is markdown — return it verbatim and let
// the model follow its instructions.
type ReadSkill struct {
	Registry *Registry
}

func (ReadSkill) Name() string { return "read_skill" }

func (r ReadSkill) Description() string {
	return `Load the full body of a named skill. The system prompt lists each skill's name + one-line description; call this tool to fetch the procedures, checklists, and examples for a specific skill before applying it.

Optionally pass 'topic' to load a sub-topic (some skills have a 'topics/<name>.md' deep-dive). Without 'topic' you get the main SKILL.md body.`
}

func (ReadSkill) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Skill name (matches what's listed in the system prompt)."},
  "topic":{"type":"string","description":"Optional sub-topic from the skill's topics/ directory."}
},
"required":["name"]
}`)
}

func (r ReadSkill) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if r.Registry == nil {
		return "", errors.New("no skills loaded")
	}
	var p struct {
		Name  string `json:"name"`
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Name == "" {
		return "", errors.New("name is required")
	}
	s := r.Registry.Get(p.Name)
	if s == nil {
		return "", fmt.Errorf("unknown skill %q. Available: %s", p.Name, strings.Join(r.Registry.Names(), ", "))
	}
	if p.Topic != "" {
		body, ok := s.Topics[p.Topic]
		if !ok {
			topics := make([]string, 0, len(s.Topics))
			for t := range s.Topics {
				topics = append(topics, t)
			}
			return "", fmt.Errorf("skill %q has no topic %q (available topics: %s)", p.Name, p.Topic, strings.Join(topics, ", "))
		}
		return body, nil
	}

	header := fmt.Sprintf("# Skill: %s (v%s)\n%s\n\n---\n\n", s.Name, s.Version, s.Description)
	out := header + s.Body
	if len(s.Topics) > 0 {
		topics := make([]string, 0, len(s.Topics))
		for t := range s.Topics {
			topics = append(topics, t)
		}
		out += fmt.Sprintf("\n\n---\n\nThis skill has topics available via read_skill(name=%q, topic=...): %s", s.Name, strings.Join(topics, ", "))
	}
	return out, nil
}
