package store

import (
	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

// cleanupIntelStaleReferences is a one-time repair for intel rows left behind
// by route-group deletions that predate group deletion cleaning up intel
// state. It removes quarantine states pointing at vanished plans or groups,
// drops orphan route_group_keys rows, and disables intel plans whose group is
// gone so neither the scheduler nor a manual trigger can fire them again.
func cleanupIntelStaleReferences(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&domain.RouteGroup{}) || !m.HasTable(&domain.IntelTestPlan{}) {
		return nil
	}
	if m.HasTable(&domain.IntelQuarantineState{}) {
		if err := db.Exec(`
			DELETE FROM intel_quarantine_states
			WHERE plan_id NOT IN (SELECT id FROM intel_test_plans)
			   OR plan_id IN (
				SELECT p.id FROM intel_test_plans p
				WHERE p.route_group_id NOT IN (SELECT id FROM route_groups)
			   )
		`).Error; err != nil {
			return err
		}
	}
	if err := db.Model(&domain.IntelTestPlan{}).
		Where("enabled = ? AND route_group_id NOT IN (SELECT id FROM route_groups)", true).
		Update("enabled", false).Error; err != nil {
		return err
	}
	if m.HasTable(&domain.RouteGroupKey{}) && m.HasTable(&domain.PlatformKey{}) {
		if err := db.Exec(`
			DELETE FROM route_group_keys
			WHERE route_group_id NOT IN (SELECT id FROM route_groups)
			   OR platform_key_id NOT IN (SELECT id FROM platform_keys)
		`).Error; err != nil {
			return err
		}
	}
	return nil
}
