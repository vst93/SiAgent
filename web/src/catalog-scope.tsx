import { useEffect, useState } from "react";

// The settings catalog is scoped: you are always looking at one Agent's view of
// the shared library. The choice persists so returning to the page lands where
// you left it, which is what keeps "upload here" unambiguous.
const scopeKey = "siagent.catalog-agent";

export function storedCatalogAgent(): string {
  try {
    return localStorage.getItem(scopeKey) ?? "";
  } catch {
    return "";
  }
}

export function useCatalogScope(
  agents: { id: string; name: string; archived?: boolean }[],
) {
  const [agentID, setAgentID] = useState(storedCatalogAgent);
  // An archived or deleted Agent must not leave the page scoped to nothing.
  const live = agents.filter((agent) => !agent.archived);
  const current = live.find((agent) => agent.id === agentID) ?? live[0] ?? null;
  useEffect(() => {
    if (current && current.id !== agentID) setAgentID(current.id);
  }, [current?.id]);
  useEffect(() => {
    try {
      if (agentID) localStorage.setItem(scopeKey, agentID);
    } catch {
      // The scope still applies for this session.
    }
  }, [agentID]);
  return {
    agents: live,
    agent: current,
    agentID: current?.id ?? "",
    select: setAgentID,
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
