package retry

import "time"

// Do 固定间隔有界重试:fn 共执行 attempts 次,全部失败返回最后一次错误。
// attempts < 1 视为 1。内部 sleep 会阻塞调用方,仅适合 run 等串行一次性执行,
// 常驻调度器(scheduler)应用自身状态机计次,避免卡住其他任务。
func Do(attempts int, interval time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(interval)
		}
		if err = fn(); err == nil {
			return nil
		}
	}
	return err
}
