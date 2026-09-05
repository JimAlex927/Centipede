package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"centipede/internal/modules/docmost/enterprise"
)

func main() {
	secret := flag.String("secret", "dev-only-change-me-please", "HMAC signing secret; must match auth.jwt_secret on the API")
	customer := flag.String("customer", "", "license customer name")
	seats := flag.Int("seats", 1, "maximum active workspace members")
	licenseType := flag.String("type", enterprise.LicenseTypeEnterprise, "license type: business or enterprise")
	days := flag.Int("days", 365, "license validity in days")
	features := flag.String("features", "", "comma-separated feature names; empty enables all features")
	workspaceID := flag.String("workspace-id", "", "optional workspace UUID binding")
	trial := flag.Bool("trial", false, "mark the license as a trial")
	out := flag.String("out", "", "write the license key to this file instead of stdout")
	flag.Parse()

	if strings.TrimSpace(*secret) == "" || strings.TrimSpace(*customer) == "" {
		fatal("-secret and -customer are required")
	}
	if *days < 1 {
		fatal("-days must be positive")
	}

	service := enterprise.NewLicenseService(*secret)
	token, _, err := service.Generate(enterprise.GenerateInput{
		CustomerName: *customer,
		SeatCount:    *seats,
		LicenseType:  *licenseType,
		ExpiresAt:    time.Now().UTC().AddDate(0, 0, *days),
		Trial:        *trial,
		Features:     splitFeatures(*features),
		WorkspaceID:  *workspaceID,
	})
	if err != nil {
		fatal(err.Error())
	}
	if *out == "" {
		fmt.Println(token)
		return
	}
	if err := os.WriteFile(*out, []byte(token+"\n"), 0600); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("license written to %s\n", *out)
}

func splitFeatures(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "license:", message)
	os.Exit(2)
}
