import { useEffect } from "react";

// One Agent is the current space for the whole workbench, not just for chat.
// Every settings page reads this same scope, so "the Agent I am working on" has
// exactly one answer and the title bar can state it.
const scopeKey = "siagent.space-agent";

// "all" is a real scope, not a missing selection: it shows every Agent's
// resources at once for cross-Agent work (audits, bulk cleanup, comparing two
// Agents). A specific Agent shows only what belongs to it.
export const ALL_SPACE = "all";

export function isAllSpace(id: string) {
  return id === ALL_SPACE;
}

export function storedSpaceAgent(): string {
  try {
    return localStorage.getItem(scopeKey) ?? "";
  } catch {
    return "";
  }
}

export function rememberSpaceAgent(id: string) {
  try {
    localStorage.setItem(scopeKey, id);
  } catch {
    // The scope still applies for this session.
  }
}

// The scope is owned by the workbench shell so the title bar, chat and every
// settings page agree on which Agent is current. This hook only resolves that
// shared id into a usable Agent and filters archived ones out of the picker.
export function useSpaceScope(
  agents: { id: string; name: string; archived?: boolean }[],
  agentID: string,
  select: (id: string) => void,
) {
  const live = agents.filter((agent) => !agent.archived);
  const current = live.find((agent) => agent.id === agentID) ?? live[0] ?? null;
  useEffect(() => {
    if (current && current.id !== agentID) select(current.id);
  }, [current?.id, agentID]);
  return {
    agents: live,
    agent: current,
    agentID: current?.id ?? "",
    select,
  };
}

export type LinkedAgent = { id: string; name: string; archived: boolean };
export type CapabilityLinks = {
  skills: Record<string, LinkedAgent[]>;
  mcps: Record<string, LinkedAgent[]>;
  knowledge: Record<string, LinkedAgent[]>;
};

export const emptyLinks: CapabilityLinks = {
  skills: {},
  mcps: {},
  knowledge: {},
};

// Three-way split that answers the question the flat list could not: is this
// mine, is it shared with me, or is it somebody else's? Shared rows are the ones
// mounted on the current Agent and at least one other, so the badge is derived
// rather than declared.
export function groupByScope<T extends { id: string }>(
  items: T[],
  links: Record<string, LinkedAgent[]>,
  agentID: string,
) {
  const mounted: { item: T; others: LinkedAgent[] }[] = [];
  const available: { item: T; others: LinkedAgent[] }[] = [];
  const unused: T[] = [];
  for (const item of items) {
    const users = links[item.id] ?? [];
    const others = users.filter((agent) => agent.id !== agentID);
    if (users.some((agent) => agent.id === agentID)) mounted.push({ item, others });
    else if (others.length) available.push({ item, others });
    else unused.push(item);
  }
  return { mounted, available, unused };
}
