package store

import "context"

// SetDefaultOfferStateForTest records a default-offer state directly, so a
// test can stand the database where a start that died mid-way left it.
func (s *Store) SetDefaultOfferStateForTest(ctx context.Context, state string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return setDefaultOffer(ctx, conn, state, "")
}
