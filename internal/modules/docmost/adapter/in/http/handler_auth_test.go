package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type recordingMailer struct {
	to      string
	subject string
	body    string
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
