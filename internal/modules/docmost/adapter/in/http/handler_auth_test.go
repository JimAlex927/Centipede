package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type recordingMailer struct {
	to      string
	subject string
	body    string
}

func TestMFARequired(t *testing.T) {
	tests := []struct {
		name      string
		record    postgres.MFARecord
		lookupErr error
		enforced  bool
		want      bool
	}{
		{name: "enabled user mfa", record: postgres.MFARecord{IsEnabled: true}, want: true},
		{name: "workspace enforcement without setup", lookupErr: postgres.ErrNotFound, enforced: true, want: true},
		{name: "workspace enforcement with disabled mfa", record: postgres.MFARecord{}, enforced: true, want: true},
		{name: "optional disabled mfa", record: postgres.MFARecord{}, want: false},
		{name: "lookup failure", lookupErr: errors.New("database unavailable"), enforced: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mfaRequired(test.record, test.lookupErr, test.enforced); got != test.want {
				t.Fatalf("mfaRequired() = %v, want %v", got, test.want)
			}
		})
	}
}

func (mailer *recordingMailer) Enabled() bool { return true }

func (mailer *recordingMailer) Send(_ context.Context, to, subject, body string) error {
	mailer.to, mailer.subject, mailer.body = to, subject, body
	return nil
}

func TestSendInvitationEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest("POST", "/", nil)
	mailer := &recordingMailer{}
	handler := &Handler{mailer: mailer, frontendURL: "http://localhost:3000"}
	handler.sendInvitationEmail(ginContext, domain.Invitation{ID: "invite", Token: "token", Email: "person@example.com", Role: "member"})
	if mailer.to != "person@example.com" || mailer.subject == "" || !strings.Contains(mailer.body, "http://localhost:3000/invites/invite?token=token") {
		t.Fatalf("unexpected invitation email: %#v", mailer)
	}
}
