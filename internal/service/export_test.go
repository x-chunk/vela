package service

// SetJanitorTaskHook lets external tests pause a janitor pass at a task.
func SetJanitorTaskHook(s *Service, f func(task string)) { s.beforeJanitorTask = f }

// DecorateStore lets external tests coordinate repository reads without hooks
// in production code. It must be called before concurrent service operations.
func DecorateStore(s *Service, wrap func(Store) Store) { s.store = wrap(s.store) }
