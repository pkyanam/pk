package skillfrontmatter

import (
	"strings"
	"testing"
)

func TestParseCommonYAMLScalarStyles(t *testing.T) {
	for _, test := range []struct {
		name, yaml, want string
	}{
		{"folded chomping", "description: >-\n  Search source code and\n  follow project conventions.\n", "Search source code and follow project conventions."},
		{"literal", "description: |\n  First line.\n  Second line.\n", "First line.\nSecond line."},
		{"folded blank paragraph", "description: >\n  First line.\n\n  Second paragraph.\n", "First line.\nSecond paragraph."},
		{"quoted colon", "description: \"Use labels: names, not commands.\"\n", "Use labels: names, not commands."},
		{"single quote escape", "description: 'The ''review'' skill.'\n", "The 'review' skill."},
		{"multiline quoted", "description: \"A long description that\n  continues on this line.\"\n", "A long description that continues on this line."},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse([]byte("---\nname: test-skill\n" + test.yaml + "---\nbody\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "test-skill" || got.Description != test.want {
				t.Fatalf("metadata = %#v, want description %q", got, test.want)
			}
		})
	}
}

func TestParseAllowsUnknownMetadataAndCRLF(t *testing.T) {
	got, err := Parse([]byte("---\r\nname: test\r\ndescription: >\r\n  Useful skill.\r\nlicense: MIT\r\nmetadata:\r\n  tags: [search, review]\r\n---\r\n"))
	if err != nil || got.Name != "test" || got.Description != "Useful skill." {
		t.Fatalf("Parse() = %#v, %v", got, err)
	}
}

func TestParseRejectsDuplicateRequiredFieldsAliasesAndBounds(t *testing.T) {
	for _, input := range []string{
		"---\nname: one\nname: two\ndescription: okay\n---\n",
		"---\nname: test\ndescription: &desc okay\nalias: *desc\n---\n",
		"---\nname: test\ndescription: [not, scalar]\n---\n",
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("Parse(%q) succeeded, want validation error", input)
		}
	}
	if _, err := Parse([]byte("---\nname: test\ndescription: " + strings.Repeat("x", MaxBytes) + "\n---\n")); err == nil {
		t.Fatal("oversized metadata should be rejected")
	}
}
