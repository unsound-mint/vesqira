import { distinguishNames, distinguishPaths } from "./distinguish";

const BASE = "vgt_Tb_combined_mw_full_hnv_res1_3pperb_reprojected_on_gass";

describe("distinguishNames", () => {
  it("keeps the end of run folders that share a long prefix", () => {
    const names = [`${BASE}_v2p0_to_8p0kms`, `${BASE}_vm2p0_to_2p0kms`, `${BASE}_v0p0_to_4p0kms`];
    expect(distinguishNames(names)).toEqual(["…v2p0_to_8p0kms", "…vm2p0_to_2p0kms", "…v0p0_to_4p0kms"]);
  });

  it("keeps the setting name of a key=value label whole", () => {
    expect(distinguishNames(["input=Tb_combined_a · ker=8", "input=Tb_combined_a · ker=4"])).toEqual(["…ker=8", "…ker=4"]);
  });

  it("elides a long shared tail as well, when the names differ in the middle", () => {
    const out = distinguishNames(["psi_hnv1_reprojected_on_gass.fits", "psi_hnv2_reprojected_on_gass.fits"]);
    expect(out).toEqual(["…hnv1…", "…hnv2…"]);
  });

  it("never cuts inside a word and never empties a name that starts another", () => {
    expect(distinguishNames(["run_a", "run_a_long"])).toEqual(["…a", "…a_long"]);
    expect(distinguishNames(["abcdef1", "abcdef2"])).toEqual(["abcdef1", "abcdef2"]);
  });

  it("leaves a single name, and equal names, whole", () => {
    expect(distinguishNames([`${BASE}_v2p0_to_8p0kms`])).toEqual([`${BASE}_v2p0_to_8p0kms`]);
    expect(distinguishNames(["a_x", "a_x"])).toEqual(["a_x", "a_x"]);
  });

  it("gives every name of the list a label that differs from the others", () => {
    const names = ["a_run_1p0", "a_run_2p0", "a_run_10p0"];
    const out = distinguishNames(names);
    expect(new Set(out).size).toBe(names.length);
  });
});

describe("distinguishPaths", () => {
  const root = "/shared/home/me/vgt/validation_output";

  it("uses the folder that differs for one run folder under fg_slabs and fg_sliding", () => {
    const run = `${BASE}_vm2p0_to_2p0kms`;
    const out = distinguishPaths([`${root}/fg_slabs/${run}/psi_reliability_x.fits`, `${root}/fg_sliding/${run}/psi_reliability_x.fits`]);
    expect(out).toEqual([`fg_slabs/${run}/psi_reliability_x.fits`, `fg_sliding/${run}/psi_reliability_x.fits`]);
  });

  it("is the last segment for paths that are already unique, and extends only the ones that collide", () => {
    expect(distinguishPaths(["/a/x/psi.fits", "/b/x/psi.fits", "/a/other.fits"])).toEqual(["a/x/psi.fits", "b/x/psi.fits", "other.fits"]);
  });

  it("copes with one path ending in another and with identical paths", () => {
    expect(distinguishPaths(["/a/x.fits", "/b/a/x.fits"])).toEqual(["a/x.fits", "b/a/x.fits"]);
    expect(distinguishPaths(["/a/x.fits", "/a/x.fits"])).toEqual(["a/x.fits", "a/x.fits"]);
  });

  it("collapses a folder that every label shares, so the one that differs stands out", () => {
    const run = `${BASE}_vm2p0_to_2p0kms`;
    const out = distinguishPaths([`${root}/fg_slabs/${run}/psi.fits`, `${root}/fg_sliding/${run}/psi.fits`], { collapseShared: true });
    expect(out).toEqual(["fg_slabs/…/psi.fits", "fg_sliding/…/psi.fits"]);
    expect(distinguishPaths(["/a/x/psi.fits", "/b/x/psi.fits"], { collapseShared: true })).toEqual(["a/x/psi.fits", "b/x/psi.fits"].map((l) => l.replace("/x/", "/…/")));
  });
});
