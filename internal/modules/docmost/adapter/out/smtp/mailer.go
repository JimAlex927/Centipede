package smtp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	stdsmtp "net/smtp"
	"strings"
	"time"

	"centipede/internal/platform/config"
)

type Mailer struct {
	config config.MailConfig
}

func New(mailConfig config.MailConfig) *Mailer { return &Mailer{config: mailConfig} }

func (mailer *Mailer) Enabled() bool { return mailer != nil && mailer.config.Enabled() }

func (mailer *Mailer) Send(ctx context.Context, to, subject, textBody string) error {
	if !mailer.Enabled() {
		return errors.New("mail is not configured")
	}
	from, err := mail.ParseAddress(mailer.config.From)
	if err != nil {
		return fmt.Errorf("parse sender: %w", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("parse recipient: %w", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("mail subject contains a line break")
	}
	address := net.JoinHostPort(mailer.config.Host, fmt.Sprintf("%d", mailer.config.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	var connection net.Conn
	if mailer.config.TLSMode == "tls" {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: mailer.config.Host, MinVersion: tls.VersionTLS12})
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("connect smtp: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	}

	client, err := stdsmtp.NewClient(connection, mailer.config.Host)
	if err != nil {
		return fmt.Errorf("create smtp client: %w", err)
	}
	defer client.Close()
	if mailer.config.TLSMode == "starttls" {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return errors.New("smtp server does not support STARTTLS")
		}
		if err = client.StartTLS(&tls.Config{ServerName: mailer.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start smtp tls: %w", err)
		}
	}
	if mailer.config.Username != "" {
		if err = client.Auth(stdsmtp.PlainAuth("", mailer.config.Username, mailer.config.Password, mailer.config.Host)); err != nil {
			return fmt.Errorf("authenticate smtp: %w", err)
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp sender: %w", err)
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("smtp recipient: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	message := buildMessage(mailer.config.From, recipient.String(), subject, textBody)
	writer := bufio.NewWriter(wc)
	if _, err = io.WriteString(writer, message); err == nil {
		err = writer.Flush()
	}
	closeErr := wc.Close()
	if err != nil {
		return fmt.Errorf("write smtp message: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("finish smtp message: %w", closeErr)
	}
	if err = client.Quit(); err != nil {
		return fmt.Errorf("quit smtp: %w", err)
	}
	return nil
}

func buildMessage(from, to, subject, body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	return "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n"
}
