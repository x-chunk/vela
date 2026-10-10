package service

// SetJanitorTaskHook lets external tests pause a janitor pass at a task.
func SetJanitorTaskHook(s *Service, f func(task string)) { s.beforeJanitorTask = f }
