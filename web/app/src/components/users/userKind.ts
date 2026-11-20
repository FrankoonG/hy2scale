import { useQuery } from '@tanstack/react-query';
import { getPolicy, type PolicySet } from '@/api/policy';
import type { UserConfig, UserKind } from '@/api/types';

/** The user's exit kind (group when unset). */
export const userKind = (u: Pick<UserConfig, 'exit_kind'>): UserKind =>
  (u.exit_kind === 'policy' || u.exit_kind === 'access' ? u.exit_kind : 'group');

/** i18n key of each kind's name. */
export const KIND_LABEL: Record<UserKind, string> = {
  group: 'policy.exitGroup',
  policy: 'policy.policySet',
  access: 'users.nodeAccess',
};

const noSets: PolicySet[] = [];

/** The node's policy sets (the exit choices' query). */
export function usePolicySets(): PolicySet[] {
  const { data } = useQuery({ queryKey: ['policy'], queryFn: getPolicy, retry: false });
  return data?.policies || noSets;
}

/** A policy-kind user's rows with their policy set here (id and name; '' = off here). */
export function rowSets(u: UserConfig, sets: PolicySet[]): { id: string; policy: string; name: string }[] {
  return (u.rows || []).map((r) => {
    const policy = u.row_policy?.[r.id] || '';
    return { id: r.id, policy, name: policy ? sets.find((s) => s.id === policy)?.name || policy : '' };
  });
}
