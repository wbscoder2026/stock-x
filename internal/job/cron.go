package job

import (
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"
)

// 5 字段 crontab（分 时 日 月 周），与 CRON_SPEC 一致。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ValidateCron 校验 crontab 表达式。
func ValidateCron(spec string) error {
	_, err := cronParser.Parse(spec)
	if err != nil {
		return fmt.Errorf("无效 cron: %w", err)
	}
	return nil
}

// Scheduler 可热更新 spec。
type Scheduler struct {
	mu   sync.Mutex
	cron *cron.Cron
	spec string
}

func NewScheduler() *Scheduler { return &Scheduler{} }

func (s *Scheduler) Spec() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec
}

func (s *Scheduler) Start(spec string, fn func()) error {
	return s.Reload(spec, fn)
}

func (s *Scheduler) Reload(spec string, fn func()) error {
	if err := ValidateCron(spec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
	c := cron.New(cron.WithParser(cronParser))
	if _, err := c.AddFunc(spec, fn); err != nil {
		return err
	}
	c.Start()
	s.cron = c
	s.spec = spec
	return nil
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}
}
