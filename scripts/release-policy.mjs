import { execFileSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const number = '(0|[1-9][0-9]*)';
const tagPattern = new RegExp(`^v${number}\\.${number}\\.${number}(?:-([0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*))?$`);

function parseTag(tag) {
 const match = tagPattern.exec(tag);
 if (!match || match[4]?.split('.').some(part => /^[0-9]+$/.test(part) && part.length > 1 && part[0] === '0')) return null;
 return {parts:match.slice(1,4).map(BigInt), prerelease:!!match[4], version:tag.slice(1), series:`${match[1]}.${match[2]}`};
}

export function releasePolicy(tag, knownTags) {
 const current = parseTag(tag);
 if (!current) throw new Error('Release tag must be an image-compatible semver tag, such as v1.2.3 or v1.2.3-rc.1');
 const candidates=knownTags.map(parseTag).filter(candidate=>candidate && !candidate.prerelease);
 const isNewer=candidate=>{
  for (let i=0;i<3;i++) {
   if (candidate.parts[i] !== current.parts[i]) return candidate.parts[i] > current.parts[i];
  }
  return false;
 };
 const newer=candidates.some(isNewer);
 const newerInSeries=candidates.some(candidate=>candidate.series===current.series && isNewer(candidate));
 return {version:current.version, series:current.series, prerelease:current.prerelease, latest:!current.prerelease && !newer, series_latest:!current.prerelease && !newerInSeries};
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
 const tags = execFileSync('git',['tag','--list'],{encoding:'utf8'}).split(/\r?\n/);
 const policy = releasePolicy(process.env.RELEASE_TAG ?? '', tags);
 const output = Object.entries(policy).map(([key,value])=>`${key}=${value}\n`).join('');
 if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT,output);
 else process.stdout.write(output);
}
