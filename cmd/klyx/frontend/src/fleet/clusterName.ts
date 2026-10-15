// clusterDisplayName shortens an EKS ARN to its cluster name; any other
// name is shown as it is.
export function clusterDisplayName(name: string): string {
  const eksClusterMarker = ":cluster/";
  const markerAt = name.lastIndexOf(eksClusterMarker);
  if (markerAt < 0) return name;
  return name.slice(markerAt + eksClusterMarker.length) || name;
}
