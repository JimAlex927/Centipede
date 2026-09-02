package banner

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderIncludesTitleAndFields(t *testing.T) {
	output := Render("Centipede", WithColor(false), WithEnv("test"), WithField("config", "local"))
	if strings.Contains(output, "ALLMACHT") || !strings.Contains(output, "CCC  EEEEE") {
		t.Fatalf("default logo should be Centipede, got %s", output)
	}
	for _, value := range []string{"Centipede", "env=test", "config=local"} {
		if !strings.Contains(output, value) {
			t.Fatalf("rendered banner does not contain %q: %s", value, output)
		}
	}
}

func TestPrintUsesConfiguredWriter(t *testing.T) {
	var output bytes.Buffer
	Print("Centipede", WithWriter(&output), WithColor(false))
	if !strings.Contains(output.String(), "Centipede") {
		t.Fatalf("expected banner in configured writer, got %q", output.String())
	}
}
