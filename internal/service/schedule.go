package service

import (
	"context"
	"errors"
	"time"
)

// Rebuilder — ночная пересборка срезов.
//
// Существует потому, что реестр обязан быть свежим к утру без участия человека:
// PM открывает список задач и видит вчерашний день, а не вчерашнюю неделю.
//
// Считать здесь нечего — вся работа в Service.Rebuild. Задача этого типа одна:
// разбудить сборку в нужный час по нужному поясу и не сломаться, если что-то
// пойдёт не так.
type Rebuilder struct {
	svc *Service
	at  time.Time // час и минута запуска; дата в значении не используется
	loc *time.Location
}

// NewRebuilder собирает ночную пересборку на час hh:mm в поясе loc.
func NewRebuilder(svc *Service, hour, minute int, loc *time.Location) *Rebuilder {
	if loc == nil {
		loc = time.UTC
	}
	return &Rebuilder{
		svc: svc,
		at:  time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC),
		loc: loc,
	}
}

// Run будит пересборку до отмены контекста.
//
// Отдельная горутина не создаётся: вызывающий сам решает, где этому жить, и
// видит завершение. Контекст отменяется вместе с сервером, и незавершённая
// сборка обрывается на своей задаче, а не бросается посреди записи: Rebuild
// пишет срез одним вызовом хранилища.
func (r *Rebuilder) Run(ctx context.Context) {
	for {
		wait := r.until(r.svc.now())
		r.svc.log.Info("ночная пересборка запланирована",
			"через", wait.Truncate(time.Minute).String(),
			"пояс", r.loc.String())

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		r.RunOnce(ctx)
	}
}

// until — сколько ждать до ближайшего срабатывания.
//
// Считается в поясе запуска, а не в UTC: «20:00 по Москве» обязано остаться
// восемью вечера и после перевода часов, и на сервере, живущем по Гринвичу.
func (r *Rebuilder) until(now time.Time) time.Duration {
	local := now.In(r.loc)
	next := time.Date(local.Year(), local.Month(), local.Day(),
		r.at.Hour(), r.at.Minute(), 0, 0, r.loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(local)
}

// RunOnce пересобирает срезы всех задач один раз.
//
// Ошибка на одной задаче не останавливает остальные. Ночной проход обязан
// дойти до конца: недоступный портал или отказ модели на одной задаче — не
// повод оставить без свежего среза все прочие.
//
// Пропуск неизменившегося материала считается успехом, а не ошибкой: у
// большинства задач за сутки не появляется ничего нового, и это обычная ночь,
// а не сбой. С платной моделью это к тому же главная статья экономии.
func (r *Rebuilder) RunOnce(ctx context.Context) {
	started := r.svc.now()

	tasks, err := r.svc.store.Tasks(ctx)
	if err != nil {
		r.svc.log.Error("ночная пересборка: список задач", "ошибка", err)
		return
	}

	var built, skipped, failed int
	for _, t := range tasks {
		// Отмена проверяется перед каждой задачей: остановка сервера не должна
		// ждать, пока модель разберёт весь реестр.
		if ctx.Err() != nil {
			r.svc.log.Info("ночная пересборка прервана",
				"собрано", built, "пропущено", skipped, "с ошибкой", failed)
			return
		}

		switch _, err := r.svc.Rebuild(ctx, t.ID); {
		case err == nil:
			built++
		case errors.Is(err, ErrNoChanges):
			skipped++
		default:
			// Ошибка остаётся у своей задачи: предыдущий срез на месте, пустым
			// он не подменяется, и утром видно, какая именно задача не собралась.
			failed++
			r.svc.log.Error("ночная пересборка задачи", "задача", t.ID, "ошибка", err)
		}
	}

	r.svc.log.Info("ночная пересборка закончена",
		"задач", len(tasks), "собрано", built, "пропущено", skipped, "с ошибкой", failed,
		"заняла", r.svc.now().Sub(started).Truncate(time.Second).String())
}
