import assert from 'node:assert/strict';
import test from 'node:test';
import { releasePolicy } from './release-policy.mjs';

test('latest advances only for the newest numeric stable version', () => {
 assert.equal(releasePolicy('v1.10.0', ['v1.9.9','v1.10.0']).latest,true);
 assert.equal(releasePolicy('v1.9.9', ['v1.10.0']).latest,false);
 assert.equal(releasePolicy('v2.0.0', ['v10.0.0']).latest,false);
 assert.equal(releasePolicy('v10.0.0', ['v2.0.0']).latest,true);
});
test('prereleases cannot advance latest, and do not outrank stable tags', () => {
 assert.deepEqual(releasePolicy('v2.0.0-rc.1',['v1.0.0']),{version:'2.0.0-rc.1',series:'2.0',prerelease:true,latest:false,series_latest:false});
 assert.equal(releasePolicy('v1.0.0',['v2.0.0-rc.1']).latest,true);
});
test('reject malformed release tags before publishing', () => {
 for(const tag of ['v1.0','v01.0.0','v1.0.0;echo bad','v1.0.0-01','v1.0.0+build']) assert.throws(()=>releasePolicy(tag,[]));
});
test('comparison does not truncate large numeric components',()=>{
 assert.equal(releasePolicy('v1.9007199254740993.0',['v1.9007199254740992.99']).latest,true);
});

test('maintenance versions advance only their own series alias',()=>{
 const policy=releasePolicy('v1.2.4',['v2.0.0','v1.2.3']);
 assert.equal(policy.latest,false);
 assert.equal(policy.series_latest,true);
 assert.equal(releasePolicy('v1.2.3',['v1.2.4']).series_latest,false);
 assert.equal(releasePolicy('v1.2.5-rc.1',['v1.2.4']).series_latest,false);
});
