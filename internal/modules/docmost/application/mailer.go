package application

import "context"

type Mailer interface {
	Enabled() bool
	Send(ctx context.Context, to, subject, textBody string) error
}
