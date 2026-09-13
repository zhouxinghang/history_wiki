import type { UserRole } from '../data/authClient'
import CanonicalEntityManager, { type CanonicalEntityDefinition } from './CanonicalEntityManager'

interface RegionManagerProps {
  userRole: UserRole
  onChange?: () => void
}

const regionDefinition: CanonicalEntityDefinition = {
  resource: 'regions',
  listField: 'regions',
  title: '地区管理',
  singular: '地区',
  description: '同名地区可以分别创建，并通过消歧名称确认语境。',
  versionConflictCode: 'region_version_conflict',
}

export default function RegionManager({ userRole, onChange }: RegionManagerProps) {
  return <CanonicalEntityManager definition={regionDefinition} userRole={userRole} onChange={onChange} />
}
