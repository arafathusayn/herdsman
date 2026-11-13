// Imported plain rows must not be mistaken for a reactive store by the build.
let reads = 0;
export const labelReads = () => reads;
export const initialRows = ["one", "two"].map((label, i) => ({
  id: i + 1,
  get label() {
    reads++;
    return label;
  },
}));
