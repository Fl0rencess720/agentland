package data

import (
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFinalizeMessagePageReturnsLatestPageInAscendingOrder(t *testing.T) {
	items := []*models.Message{{ID: "m5"}, {ID: "m4"}, {ID: "m3"}, {ID: "m2"}}
	page, next, err := finalizeMessagePage(items, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"m3", "m4", "m5"}, []string{page[0].ID, page[1].ID, page[2].ID})
	require.NotNil(t, next)
	require.Equal(t, "m3", *next)
}

func TestFinalizeMessagePageEndsHistoryWithoutCursor(t *testing.T) {
	items := []*models.Message{{ID: "m2"}, {ID: "m1"}}
	page, next, err := finalizeMessagePage(items, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2"}, []string{page[0].ID, page[1].ID})
	require.Nil(t, next)
}
