package gametime

import "time"

func Now() time.Time {
	return time.Now()
}

func NowMillis() int64 {
	return Now().UnixMilli()
}

func StartOfDayMillis() int64 {
	return StartOfDayMillisAt(NowMillis())
}

func StartOfDayMillisAt(millis int64) int64 {
	n := time.UnixMilli(millis).In(time.Local)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).UnixMilli()
}

// WeeklyVersion returns a stable weekly identifier using the system local
// timezone (start-of-week timestamp in millis, Monday 00:00 local time).
func WeeklyVersion(millis int64) int64 {
	t := time.UnixMilli(millis).In(time.Local)
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := time.Date(t.Year(), t.Month(), t.Day()-(weekday-1), 0, 0, 0, 0, time.Local)
	return monday.UnixMilli()
}

// MonthlyVersion returns a stable monthly identifier using the system local
// timezone (start-of-month timestamp in millis, 1st day 00:00 local time).
func MonthlyVersion(millis int64) int64 {
	t := time.UnixMilli(millis).In(time.Local)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local).UnixMilli()
}
