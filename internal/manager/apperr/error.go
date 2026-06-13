package apperr

type Error struct {
	Status  int
	Message string
}

func (e Error) Error() string {
	return e.Message
}
