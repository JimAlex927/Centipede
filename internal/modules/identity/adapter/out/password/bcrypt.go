package password

import "golang.org/x/crypto/bcrypt"

type Hasher struct {
	Cost int
}

func (hasher Hasher) Hash(raw string) (string, error) {
	cost := hasher.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(raw), cost)
	return string(hashed), err
}

func (hasher Hasher) Compare(encoded, raw string) error {
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(raw))
}
