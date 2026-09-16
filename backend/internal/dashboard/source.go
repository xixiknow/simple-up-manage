package dashboard

import (
	"context"

	"simple-up-manage/internal/domain"
)

type sourceKey struct{}

func WithSource(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sourceKey{}, source)
}

func SourceFrom(ctx context.Context) string {
	if ctx == nil {
		return domain.SourceBusiness
	}
	if v, ok := ctx.Value(sourceKey{}).(string); ok && v != "" {
		return v
	}
	return domain.SourceBusiness
}

func IsBusiness(ctx context.Context) bool {
	return SourceFrom(ctx) == domain.SourceBusiness
}
