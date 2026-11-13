/**
 * The background surveys the sky panel offers (ADR 0072). Each is a public HiPS served by CDS (Strasbourg). The service URL is the one
 * the CDS HiPS registry (MocServer, `hips_service_url`) lists for that id, so Aladin Lite is given the address itself and never
 * asks a registry to resolve an id: the only server contacted is `host`.
 */
export type Survey = {
  /** The IVOA HiPS identifier, recorded with anything that includes the background. */
  id: string;
  name: string;
  /** Who serves the tiles. */
  provider: string;
  host: string;
  url: string;
  /** The data's own owner, for the credit line. */
  credit: string;
};

const CDS = "CDS, Strasbourg";
const HOST = "alasky.cds.unistra.fr";

export const SURVEYS: Survey[] = [
  { id: "CDS/P/DSS2/color", name: "DSS2 colour (optical)", provider: CDS, host: HOST, url: "https://alasky.cds.unistra.fr/DSS/DSSColor", credit: "Digitized Sky Survey, STScI/NASA; coloured and healpixed by CDS" },
  { id: "CDS/P/2MASS/color", name: "2MASS colour J, H, K (near infrared)", provider: CDS, host: HOST, url: "https://alasky.cds.unistra.fr/2MASS/Color", credit: "2MASS, University of Massachusetts and IPAC/Caltech" },
  { id: "CDS/P/allWISE/color", name: "AllWISE colour W4, W2, W1 (mid infrared)", provider: CDS, host: HOST, url: "https://alasky.cds.unistra.fr/AllWISE/RGB-W4-W2-W1", credit: "WISE, IPAC/NASA" },
  { id: "CDS/P/PanSTARRS/DR1/color-z-zg-g", name: "Pan-STARRS DR1 colour (optical)", provider: CDS, host: HOST, url: "https://alasky.cds.unistra.fr/Pan-STARRS/DR1/color-z-zg-g", credit: "Pan-STARRS1 Science Consortium, via MAST/STScI" },
  { id: "CDS/P/HI4PI/NHI", name: "HI4PI H I column density", provider: CDS, host: HOST, url: "https://alasky.cds.unistra.fr/HI4PI/P_HI4PI_NHI", credit: "HI4PI Collaboration (B. Winkel)" },
];

export const DEFAULT_SURVEY = SURVEYS[0]!.id;
export const surveyById = (id: string): Survey => SURVEYS.find((s) => s.id === id) ?? SURVEYS[0]!;

/** The sentence the person reads before the background is turned on, and again beside the switch while it is on. */
export function contactNotice(s: Survey): string {
  return `Turning this on contacts ${s.host}, the HiPS server of ${s.provider}. It receives the parts of the sky you look at (the image tiles your position and zoom request) and your IP address. The view centres on your datasets, so those requests show which field you are working on. No dataset, file name, path or pixel value is sent. With it off, nothing leaves this computer.`;
}
